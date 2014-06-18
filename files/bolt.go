package files

import (
	"strconv"

	"github.com/boltdb/bolt"
	"github.com/calmh/syncthing/scanner"
)

/*
Bolt DB structure:

- files
 	- <node id>
 		- <repo>
 			* name -> scanner.File
- global
 	- repo
		* name -> version

*/

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
