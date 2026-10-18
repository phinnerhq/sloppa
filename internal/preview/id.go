package preview

import "crypto/rand"

const base36 = "0123456789abcdefghijklmnopqrstuvwxyz"

// newID is 8 base36 characters, about 41 bits: unguessable at the proxy's rate limit, short enough
// to read out.
func newID() string {
	id := make([]byte, 0, 8)
	var random [16]byte
	for len(id) < 8 {
		rand.Read(random[:])
		for _, b := range random {
			// 252 is the largest multiple of 36 below 256: rejecting the rest keeps every character
			// equally likely.
			if b < 252 && len(id) < 8 {
				id = append(id, base36[b%36])
			}
		}
	}
	return string(id)
}
