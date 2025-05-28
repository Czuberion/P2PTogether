// peer_service/internal/common/cid.go
package common

import (
	"github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// StringToCID converts an arbitrary string to a CID.
// Uses CIDv1, Raw codec, SHA2-256 multihash.
func StringToCID(data string) (cid.Cid, error) {
	pref := cid.Prefix{
		Version:  1,
		Codec:    cid.Raw,
		MhType:   mh.SHA2_256,
		MhLength: -1, // Default length for SHA2-256
	}
	return pref.Sum([]byte(data))
}
