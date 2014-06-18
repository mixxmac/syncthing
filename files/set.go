// Copyright (C) 2014 Jakob Borg and other contributors. All rights reserved.
// Use of this source code is governed by an MIT-style license that can be
// found in the LICENSE file.

// Package files provides a set type to track local/remote files with newness checks.
package files

import (
	"errors"
	"strconv"
	"sync"

	"github.com/boltdb/bolt"
	"github.com/calmh/syncthing/cid"
	"github.com/calmh/syncthing/lamport"
	"github.com/calmh/syncthing/protocol"
	"github.com/calmh/syncthing/scanner"
)

type fileRecord struct {
	File   scanner.File
	Usage  int
	Global bool
}

type bitset uint64

type Set struct {
	sync.Mutex
	files              map[key]fileRecord
	remoteKey          [64]map[string]key
	changes            [64]uint64
	globalAvailability map[string]bitset
	globalKey          map[string]key
	globalVersion      map[string]uint64
	repo               string
	db                 *bolt.DB
}

func NewSet(repo string, db *bolt.DB) *Set {
	var m = Set{
		files:              make(map[key]fileRecord),
		globalAvailability: make(map[string]bitset),
		globalKey:          make(map[string]key),
		globalVersion:      make(map[string]uint64),
		repo:               repo,
		db:                 db,
	}
	return &m
}

func boltReplace(id uint, repo string, fs []scanner.File) func(tx *bolt.Tx) error {
	return func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte("files"))
		if err != nil {
			return err
		}

		bktName := []byte(strconv.FormatUint(uint64(id), 16))

		bkt.DeleteBucket(bktName)
		bkt, err = bkt.CreateBucket(bktName)
		if err != nil {
			return err
		}

		bkt, err = bkt.CreateBucket([]byte(repo))
		if err != nil {
			return err
		}

		return boltUpdateBucket(bkt, fs)
	}
}

func boltUpdate(id uint, repo string, fs []scanner.File) func(tx *bolt.Tx) error {
	return func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte("files"))
		if err != nil {
			return err
		}

		bktName := []byte(strconv.FormatUint(uint64(id), 16))

		bkt, err = bkt.CreateBucketIfNotExists(bktName)
		if err != nil {
			return err
		}

		bkt, err = bkt.CreateBucketIfNotExists([]byte(repo))
		if err != nil {
			return err
		}

		return boltUpdateBucket(bkt, fs)
	}
}

func boltUpdateBucket(bkt *bolt.Bucket, fs []scanner.File) error {
	for _, f := range fs {
		key := []byte(f.Name)
		err := bkt.Put(key, f.MarshalXDR())
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Set) Replace(id uint, fs []scanner.File) {
	if debug {
		l.Debugf("Replace(%d, [%d])", id, len(fs))
	}
	if id > 63 {
		panic("Connection ID must be in the range 0 - 63 inclusive")
	}

	m.db.Update(boltReplace(id, m.repo, fs))

	m.Lock()
	if len(fs) == 0 || !m.equals(id, fs) {
		m.changes[id]++
		m.replace(id, fs)
	}
	m.Unlock()
}

func (m *Set) ReplaceWithDelete(id uint, fs []scanner.File) {
	if debug {
		l.Debugf("ReplaceWithDelete(%d, [%d])", id, len(fs))
	}
	if id > 63 {
		panic("Connection ID must be in the range 0 - 63 inclusive")
	}

	var nm = make(map[string]bool, len(fs))
	for _, f := range fs {
		nm[f.Name] = true
	}

	m.db.Update(func(tx *bolt.Tx) error {
		err := boltUpdate(id, m.repo, fs)(tx)
		if err != nil {
			return err
		}

		bkt := tx.Bucket([]byte("files"))
		if bkt == nil {
			return errors.New("no root bucket")
		}

		bktName := []byte(strconv.FormatUint(uint64(id), 16))
		bkt = bkt.Bucket(bktName)
		if bkt == nil {
			return errors.New("no id bucket")
		}

		bktName = []byte(m.repo)
		bkt = bkt.Bucket(bktName)
		if bkt == nil {
			return errors.New("no repo bucket")
		}

		c := bkt.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var f scanner.File
			f.UnmarshalXDR(v)
			if !nm[f.Name] && !protocol.IsDeleted(f.Flags) {
				f.Flags |= protocol.FlagDeleted
				f.Blocks = nil
				bkt.Put(k, f.MarshalXDR())
			}
		}

		return nil
	})

	m.Lock()
	if len(fs) == 0 || !m.equals(id, fs) {
		m.changes[id]++

		var nf = make(map[string]key, len(fs))
		for _, f := range fs {
			nf[f.Name] = keyFor(f)
		}

		// For previously existing files not in the list, add them to the list
		// with the relevant delete flags etc set. Previously existing files
		// with the delete bit already set are not modified.

		for _, ck := range m.remoteKey[cid.LocalID] {
			if _, ok := nf[ck.Name]; !ok {
				cf := m.files[ck].File
				if !protocol.IsDeleted(cf.Flags) {
					cf.Flags |= protocol.FlagDeleted
					cf.Blocks = nil
					cf.Size = 0
					cf.Version = lamport.Default.Tick(cf.Version)
				}
				fs = append(fs, cf)
				if debug {
					l.Debugln("deleted:", ck.Name)
				}
			}
		}

		m.replace(id, fs)
	}
	m.Unlock()
}

func (m *Set) Update(id uint, fs []scanner.File) {
	if debug {
		l.Debugf("Update(%d, [%d])", id, len(fs))
	}

	m.db.Update(boltUpdate(id, m.repo, fs))

	m.Lock()
	m.update(id, fs)
	m.changes[id]++
	m.Unlock()
}

func (m *Set) Need(id uint) []scanner.File {
	if debug {
		l.Debugf("Need(%d)", id)
	}
	m.Lock()
	var fs = make([]scanner.File, 0, len(m.globalKey)/2) // Just a guess, but avoids too many reallocations
	rkID := m.remoteKey[id]
	for gk, gf := range m.files {
		if !gf.Global || gf.File.Suppressed {
			continue
		}

		if rk, ok := rkID[gk.Name]; gk.newerThan(rk) {
			if protocol.IsDeleted(gf.File.Flags) && (!ok || protocol.IsDeleted(m.files[rk].File.Flags)) {
				// We don't need to delete files we don't have or that are already deleted
				continue
			}

			fs = append(fs, gf.File)
		}
	}
	m.Unlock()
	return fs
}

func (m *Set) Have(id uint) []scanner.File {
	if debug {
		l.Debugf("Have(%d)", id)
	}

	var fs []scanner.File

	m.db.View(func(tx *bolt.Tx) error {
		bkt := tx.Bucket([]byte("files"))
		if bkt == nil {
			return nil
		}

		bktName := []byte(strconv.FormatUint(uint64(id), 16))
		bkt = bkt.Bucket(bktName)
		if bkt == nil {
			return nil
		}

		bkt = bkt.Bucket([]byte(m.repo))
		if bkt == nil {
			return nil
		}

		c := bkt.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var f scanner.File
			f.UnmarshalXDR(v)
			fs = append(fs, f)
		}

		return nil
	})

	return fs
}

func (m *Set) Global() []scanner.File {
	if debug {
		l.Debugf("Global()")
	}
	m.Lock()
	var fs = make([]scanner.File, 0, len(m.globalKey))
	for _, file := range m.files {
		if file.Global {
			fs = append(fs, file.File)
		}
	}
	m.Unlock()
	return fs
}

func (m *Set) Get(id uint, file string) scanner.File {
	m.Lock()
	defer m.Unlock()
	if debug {
		l.Debugf("Get(%d, %q)", id, file)
	}
	return m.files[m.remoteKey[id][file]].File
}

func (m *Set) GetGlobal(file string) scanner.File {
	m.Lock()
	defer m.Unlock()
	if debug {
		l.Debugf("GetGlobal(%q)", file)
	}
	return m.files[m.globalKey[file]].File
}

func (m *Set) Availability(name string) bitset {
	m.Lock()
	defer m.Unlock()
	av := m.globalAvailability[name]
	if debug {
		l.Debugf("Availability(%q) = %0x", name, av)
	}
	return av
}

func (m *Set) Changes(id uint) uint64 {
	m.Lock()
	defer m.Unlock()
	if debug {
		l.Debugf("Changes(%d)", id)
	}
	return m.changes[id]
}

func (m *Set) equals(id uint, fs []scanner.File) bool {
	curWithoutDeleted := make(map[string]key)
	for _, k := range m.remoteKey[id] {
		f := m.files[k].File
		if !protocol.IsDeleted(f.Flags) {
			curWithoutDeleted[f.Name] = k
		}
	}
	if len(curWithoutDeleted) != len(fs) {
		return false
	}
	for _, f := range fs {
		if curWithoutDeleted[f.Name] != keyFor(f) {
			return false
		}
	}
	return true
}

func (m *Set) update(cid uint, fs []scanner.File) {
	remFiles := m.remoteKey[cid]
	if remFiles == nil {
		l.Fatalln("update before replace for cid", cid)
	}
	for _, f := range fs {
		n := f.Name
		fk := keyFor(f)

		if ck, ok := remFiles[n]; ok && ck == fk {
			// The remote already has exactly this file, skip it
			continue
		}

		remFiles[n] = fk

		// Keep the block list or increment the usage
		if br, ok := m.files[fk]; !ok {
			m.files[fk] = fileRecord{
				Usage: 1,
				File:  f,
			}
		} else {
			br.Usage++
			m.files[fk] = br
		}

		// Update global view
		gk, ok := m.globalKey[n]
		switch {
		case ok && fk == gk:
			av := m.globalAvailability[n]
			av |= 1 << cid
			m.globalAvailability[n] = av
		case fk.newerThan(gk):
			if ok {
				f := m.files[gk]
				f.Global = false
				m.files[gk] = f
			}
			f := m.files[fk]
			f.Global = true
			m.files[fk] = f
			m.globalKey[n] = fk
			m.globalAvailability[n] = 1 << cid
		}
	}
}

func (m *Set) replace(cid uint, fs []scanner.File) {
	// Decrement usage for all files belonging to this remote, and remove
	// those that are no longer needed.
	for _, fk := range m.remoteKey[cid] {
		br, ok := m.files[fk]
		switch {
		case ok && br.Usage == 1:
			delete(m.files, fk)
		case ok && br.Usage > 1:
			br.Usage--
			m.files[fk] = br
		}
	}

	// Clear existing remote remoteKey
	m.remoteKey[cid] = make(map[string]key)

	// Recalculate global based on all remaining remoteKey
	for n := range m.globalKey {
		var nk key    // newest key
		var na bitset // newest availability

		for i, rem := range m.remoteKey {
			if rk, ok := rem[n]; ok {
				switch {
				case rk == nk:
					na |= 1 << uint(i)
				case rk.newerThan(nk):
					nk = rk
					na = 1 << uint(i)
				}
			}
		}

		if na != 0 {
			// Someone had the file
			f := m.files[nk]
			f.Global = true
			m.files[nk] = f
			m.globalKey[n] = nk
			m.globalAvailability[n] = na
		} else {
			// Noone had the file
			delete(m.globalKey, n)
			delete(m.globalAvailability, n)
		}
	}

	// Add new remote remoteKey to the mix
	m.update(cid, fs)
}
