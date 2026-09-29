package db

import (
	"crypto/rand"
	"time"
)

// crockford is the Crockford base32 alphabet ULIDs use: no I, L, O or U.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID mints a 26-character Crockford base32 ULID from the millisecond clock
// and 80 random bits; only millisecond order is guaranteed, not mint order.
func NewID() string {
	var id [16]byte

	ms := uint64(time.Now().UnixMilli())
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)

	if _, err := rand.Read(id[6:]); err != nil {
		// Only an unusable OS entropy source fails here.
		panic("db: crypto/rand: " + err.Error())
	}

	return encodeULID(id)
}

func encodeULID(id [16]byte) string {
	var out [26]byte

	out[0] = crockford[(id[0]&224)>>5]
	out[1] = crockford[id[0]&31]
	out[2] = crockford[(id[1]&248)>>3]
	out[3] = crockford[((id[1]&7)<<2)|((id[2]&192)>>6)]
	out[4] = crockford[(id[2]&62)>>1]
	out[5] = crockford[((id[2]&1)<<4)|((id[3]&240)>>4)]
	out[6] = crockford[((id[3]&15)<<1)|((id[4]&128)>>7)]
	out[7] = crockford[(id[4]&124)>>2]
	out[8] = crockford[((id[4]&3)<<3)|((id[5]&224)>>5)]
	out[9] = crockford[id[5]&31]
	out[10] = crockford[(id[6]&248)>>3]
	out[11] = crockford[((id[6]&7)<<2)|((id[7]&192)>>6)]
	out[12] = crockford[(id[7]&62)>>1]
	out[13] = crockford[((id[7]&1)<<4)|((id[8]&240)>>4)]
	out[14] = crockford[((id[8]&15)<<1)|((id[9]&128)>>7)]
	out[15] = crockford[(id[9]&124)>>2]
	out[16] = crockford[((id[9]&3)<<3)|((id[10]&224)>>5)]
	out[17] = crockford[id[10]&31]
	out[18] = crockford[(id[11]&248)>>3]
	out[19] = crockford[((id[11]&7)<<2)|((id[12]&192)>>6)]
	out[20] = crockford[(id[12]&62)>>1]
	out[21] = crockford[((id[12]&1)<<4)|((id[13]&240)>>4)]
	out[22] = crockford[((id[13]&15)<<1)|((id[14]&128)>>7)]
	out[23] = crockford[(id[14]&124)>>2]
	out[24] = crockford[((id[14]&3)<<3)|((id[15]&224)>>5)]
	out[25] = crockford[id[15]&31]

	return string(out[:])
}
