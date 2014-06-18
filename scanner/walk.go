// Copyright (C) 2014 Jakob Borg and other contributors. All rights reserved.
// Use of this source code is governed by an MIT-style license that can be
// found in the LICENSE file.

package scanner

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"code.google.com/p/go.text/unicode/norm"

	"github.com/calmh/syncthing/lamport"
	"github.com/calmh/syncthing/protocol"
)

type Walker struct {
	// Dir is the base directory for the walk
	Dir string
	// BlockSize controls the size of the block used when hashing.
	BlockSize int
	// If IgnoreFile is not empty, it is the name used for the file that holds ignore patterns.
	IgnoreFile string
	// If TempNamer is not nil, it is used to ignore tempory files when walking.
	TempNamer TempNamer
	// If CurrentFiler is not nil, it is queried for the current file before rescanning.
	CurrentFiler CurrentFiler
	// If Suppressor is not nil, it is queried for supression of modified files.
	// Suppressed files will be returned with empty metadata and the Suppressed flag set.
	// Requires CurrentFiler to be set.
	Suppressor Suppressor
	// If IgnorePerms is true, changes to permission bits will not be
	// detected. Scanned files will get zero permission bits and the
	// NoPermissionBits flag set.
	IgnorePerms bool
}

type TempNamer interface {
	// Temporary returns a temporary name for the filed referred to by filepath.
	TempName(path string) string
	// IsTemporary returns true if path refers to the name of temporary file.
	IsTemporary(path string) bool
}

type Suppressor interface {
	// Supress returns true if the update to the named file should be ignored.
	Suppress(name string, fi os.FileInfo) (bool, bool)
}

type CurrentFiler interface {
	// CurrentFile returns the file as seen at last scan.
	CurrentFile(name string) File
}

// Walk returns the list of files found in the local repository by scanning the
// file system. Files are blockwise hashed.
func (w *Walker) Walk() (files []File, ignore map[string][]string, err error) {
	if debug {
		l.Debugln("Walk", w.Dir, w.BlockSize, w.IgnoreFile)
	}

	err = checkDir(w.Dir)
	if err != nil {
		return
	}

	t0 := time.Now()

	ignore = make(map[string][]string)
	hashFiles := w.walkAndHashFiles(&files, ignore)

	filepath.Walk(w.Dir, w.loadIgnoreFiles(w.Dir, ignore))
	filepath.Walk(w.Dir, hashFiles)

	if debug {
		t1 := time.Now()
		d := t1.Sub(t0).Seconds()
		l.Debugf("Walk in %.02f ms, %.0f files/s", d*1000, float64(len(files))/d)
	}

	err = checkDir(w.Dir)
	return
}

// CleanTempFiles removes all files that match the temporary filename pattern.
func (w *Walker) CleanTempFiles() {
	filepath.Walk(w.Dir, w.cleanTempFile)
}

func (w *Walker) loadIgnoreFiles(dir string, ign map[string][]string) filepath.WalkFunc {
	return func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		rn, err := filepath.Rel(dir, p)
		if err != nil {
			return nil
		}

		if pn, sn := filepath.Split(rn); sn == w.IgnoreFile {
			pn := filepath.Clean(pn)
			l.Debugf("pn: %q", pn)
			bs, _ := ioutil.ReadFile(p)
			lines := bytes.Split(bs, []byte("\n"))
			var patterns []string
			for _, line := range lines {
				lineStr := strings.TrimSpace(string(line))
				if len(lineStr) > 0 {
					patterns = append(patterns, lineStr)
				}
			}
			ign[pn] = patterns
		}

		return nil
	}
}

func (w *Walker) walkAndHashFiles(res *[]File, ign map[string][]string) filepath.WalkFunc {
	return func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if debug {
				l.Debugln("error:", p, info, err)
			}
			return nil
		}

		rn, err := filepath.Rel(w.Dir, p)
		if err != nil {
			if debug {
				l.Debugln("rel error:", p, err)
			}
			return nil
		}

		if rn == "." {
			return nil
		}

		if w.TempNamer != nil && w.TempNamer.IsTemporary(rn) {
			// A temporary file
			if debug {
				l.Debugln("temporary:", rn)
			}
			return nil
		}

		if sn := filepath.Base(rn); sn == w.IgnoreFile || sn == ".stversions" || w.ignoreFile(ign, rn) {
			// An ignored file
			if debug {
				l.Debugln("ignored:", rn)
			}
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if (runtime.GOOS == "linux" || runtime.GOOS == "windows") && !norm.NFC.IsNormalString(rn) {
			l.Warnf("File %q contains non-NFC UTF-8 sequences and cannot be synced. Consider renaming.", rn)
			return nil
		}

		if info.Mode().IsDir() {
			if w.CurrentFiler != nil {
				cf := w.CurrentFiler.CurrentFile(rn)
				permUnchanged := w.IgnorePerms || !protocol.HasPermissionBits(cf.Flags) || PermsEqual(cf.Flags, uint32(info.Mode()))
				if cf.Modified == info.ModTime().Unix() && protocol.IsDirectory(cf.Flags) && permUnchanged {
					if debug {
						l.Debugln("unchanged:", cf)
					}
					*res = append(*res, cf)
				} else {
					var flags uint32 = protocol.FlagDirectory
					if w.IgnorePerms {
						flags |= protocol.FlagNoPermBits | 0777
					} else {
						flags |= uint32(info.Mode() & os.ModePerm)
					}
					f := File{
						Name:     rn,
						Version:  lamport.Default.Tick(0),
						Flags:    flags,
						Modified: info.ModTime().Unix(),
					}
					if debug {
						l.Debugln("dir:", cf, f)
					}
					*res = append(*res, f)
				}
				return nil
			}
		}

		if info.Mode().IsRegular() {
			if w.CurrentFiler != nil {
				cf := w.CurrentFiler.CurrentFile(rn)
				permUnchanged := w.IgnorePerms || !protocol.HasPermissionBits(cf.Flags) || PermsEqual(cf.Flags, uint32(info.Mode()))
				if !protocol.IsDeleted(cf.Flags) && cf.Modified == info.ModTime().Unix() && permUnchanged {
					if debug {
						l.Debugln("unchanged:", cf)
					}
					*res = append(*res, cf)
					return nil
				}

				if w.Suppressor != nil {
					if cur, prev := w.Suppressor.Suppress(rn, info); cur && !prev {
						l.Infof("Changes to %q are being temporarily suppressed because it changes too frequently.", p)
						cf.Suppressed = true
						cf.Version++
						if debug {
							l.Debugln("suppressed:", cf)
						}
						*res = append(*res, cf)
						return nil
					} else if prev && !cur {
						l.Infof("Changes to %q are no longer suppressed.", p)
					}
				}

				if debug {
					l.Debugln("rescan:", cf, info.ModTime().Unix(), info.Mode()&os.ModePerm)
				}
			}

			fd, err := os.Open(p)
			if err != nil {
				if debug {
					l.Debugln("open:", p, err)
				}
				return nil
			}
			defer fd.Close()

			t0 := time.Now()
			blocks, err := Blocks(fd, w.BlockSize)
			if err != nil {
				if debug {
					l.Debugln("hash error:", rn, err)
				}
				return nil
			}
			if debug {
				t1 := time.Now()
				l.Debugln("hashed:", rn, ";", len(blocks), "blocks;", info.Size(), "bytes;", int(float64(info.Size())/1024/t1.Sub(t0).Seconds()), "KB/s")
			}

			var flags = uint32(info.Mode() & os.ModePerm)
			if w.IgnorePerms {
				flags = protocol.FlagNoPermBits | 0666
			}
			f := File{
				Name:     rn,
				Version:  lamport.Default.Tick(0),
				Size:     info.Size(),
				Flags:    flags,
				Modified: info.ModTime().Unix(),
				Blocks:   blocks,
			}
			*res = append(*res, f)
		}

		return nil
	}
}

func (w *Walker) cleanTempFile(path string, info os.FileInfo, err error) error {
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeType == 0 && w.TempNamer.IsTemporary(path) {
		os.Remove(path)
	}
	return nil
}

func (w *Walker) ignoreFile(patterns map[string][]string, file string) bool {
	first, last := filepath.Split(file)
	for prefix, pats := range patterns {
		if prefix == "." || prefix == first || strings.HasPrefix(first, fmt.Sprintf("%s%c", prefix, os.PathSeparator)) {
			for _, pattern := range pats {
				l.Debugf("%q %q", pattern, last)
				if match, _ := filepath.Match(pattern, last); match {
					return true
				}
			}
		}
	}
	return false
}

func checkDir(dir string) error {
	if info, err := os.Lstat(dir); err != nil {
		return err
	} else if !info.IsDir() {
		return errors.New(dir + ": not a directory")
	} else if debug {
		l.Debugln("checkDir", dir, info)
	}
	return nil
}

func PermsEqual(a, b uint32) bool {
	switch runtime.GOOS {
	case "windows":
		// There is only writeable and read only, represented for user, group
		// and other equally. We only compare against user.
		return a&0600 == b&0600
	default:
		// All bits count
		return a&0777 == b&0777
	}
}
