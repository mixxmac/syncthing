// Copyright (C) 2014 Jakob Borg and other contributors. All rights reserved.
// Use of this source code is governed by an MIT-style license that can be
// found in the LICENSE file.

package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base32"
	"path/filepath"
	"strings"
)

func loadCert(dir string) (tls.Certificate, error) {
	cf := filepath.Join(dir, "cert.pem")
	kf := filepath.Join(dir, "key.pem")
	return tls.LoadX509KeyPair(cf, kf)
}

func certID(bs []byte) string {
	hf := sha256.New()
	hf.Write(bs)
	id := base32.StdEncoding.EncodeToString(hf.Sum(nil))
	id = strings.Trim(id, "=")
	id = strings.ToLower(id)
	return id
}
