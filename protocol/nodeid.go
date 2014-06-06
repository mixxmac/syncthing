package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/calmh/syncthing/luhn"
)

type NodeID [32]byte

func NewNodeID(rawCert []byte) NodeID {
	var n NodeID
	hf := sha256.New()
	hf.Write(rawCert)
	hf.Sum(n[:])
	return n
}

func (n NodeID) String() string {
	id := base32.StdEncoding.EncodeToString(n[:])
	id = strings.Trim(id, "=")
	id = strings.ToUpper(id)
	id = strings.Replace(id, "-", "", -1)
	id = strings.Replace(id, " ", "", -1)
	if len(id) == 54 {
		// Has check digits; discard before we renegerate
		id = id[:26] + id[27:53]
	}
	p0 := id[:26]
	c0 := luhn.Base32Trimmed.Generate(p0)
	p1 := id[26:]
	c1 := luhn.Base32Trimmed.Generate(p1)
	id = fmt.Sprintf("%s%c%s%c", p0, c0, p1, c1)
	id = regexp.MustCompile("(.{9})").ReplaceAllString(id, "$1-")
	id = strings.Trim(id, "-")
	return id
}

func (n NodeID) Compare(other NodeID) int {
	return bytes.Compare(n[:], other[:])
}

func (n NodeID) Equals(other NodeID) bool {
	return bytes.Compare(n[:], other[:]) == 0
}

func (n *NodeID) UnmarshalText(bs []byte) error {
	id := string(bs)
	id = strings.Trim(id, "=")
	id = strings.ToUpper(id)
	id = strings.Replace(id, "-", "", -1)
	id = strings.Replace(id, " ", "", -1)

	dec, err := base32.StdEncoding.DecodeString(id)
	if err != nil {
		return err
	}
	copy(n[:], dec)

	switch len(id) {
	case 52:
		return nil
	case 54:
		// New style, with check digits
		break
	default:
		return errors.New("node ID invalid: incorrect length")
	}
	p0 := id[:26]
	c0 := luhn.Base32Trimmed.Generate(p0)
	p1 := id[27:53]
	c1 := luhn.Base32Trimmed.Generate(p1)
	correct := fmt.Sprintf("%s%c%s%c", p0, c0, p1, c1)
	if correct != id {
		return errors.New("node ID invalid: incorrect check characters")
	}
	return nil
}
