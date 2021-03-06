package ksuid

import (
	"bytes"
	"crypto/rand"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	// KSUID's epoch starts more recently so that the 32-bit number space gives a
	// significantly higher useful lifetime of around 136 years from March 2017.
	// This number (14e8) was picked to be easy to remember.
	epochStamp int64 = 1400000000

	// Timestamp is a uint32
	timestampLengthInBytes = 4

	// Payload is 16-bytes
	payloadLengthInBytes = 16

	// KSUIDs are 20 bytes when binary encoded
	byteLength = timestampLengthInBytes + payloadLengthInBytes

	// The length of a KSUID when string (base62) encoded
	stringEncodedLength = 27

	// A string-encoded maximum value for a KSUID
	maxStringEncoded = "aWgEPTl1tmebfsQzFP4bxwgy80V"
)

// KSUIDs are 20 bytes:
//  00-03 byte: uint32 BE UTC timestamp with custom epoch
//  04-19 byte: random "payload"
type KSUID [byteLength]byte

var (
	rander = rand.Reader

	errSize        = fmt.Errorf("Valid KSUIDs are %v bytes", byteLength)
	errStrSize     = fmt.Errorf("Valid encoded KSUIDs are %v characters", stringEncodedLength)
	errPayloadSize = fmt.Errorf("Valid KSUID payloads are %v bytes", payloadLengthInBytes)

	// Represents a completely empty (invalid) KSUID
	Nil KSUID
)

// Append appends the string representation of i to b, returning a slice to a
// potentially larger memory area.
func (i KSUID) Append(b []byte) []byte {
	// This is an optimization that ensures we do at most one memory allocation
	// when the byte slice capacity is lower than the length of the string
	// representation of the KSUID.
	if cap(b) < stringEncodedLength {
		c := make([]byte, len(b), len(b)+stringEncodedLength)
		copy(c, b)
		b = c
	}

	off := len(b)
	b = appendEncodeBase62(b, i[:])

	if pad := stringEncodedLength - (len(b) - off); pad > 0 {
		zeroes := [...]byte{
			'0', '0', '0', '0', '0', '0', '0', '0', '0', '0',
			'0', '0', '0', '0', '0', '0', '0', '0', '0', '0',
			'0', '0', '0', '0', '0', '0', '0',
		}
		b = append(b, zeroes[:pad]...)
		copy(b[off+pad:], b[off:])
		copy(b[off:], zeroes[:pad])
	}

	return b
}

// The timestamp portion of the ID as a Time object
func (i KSUID) Time() time.Time {
	return correctedUTCTimestampToTime(i.Timestamp())
}

// The timestamp portion of the ID as a bare integer which is uncorrected
// for KSUID's special epoch.
func (i KSUID) Timestamp() uint32 {
	return binary.BigEndian.Uint32(i[:timestampLengthInBytes])
}

// The 16-byte random payload without the timestamp
func (i KSUID) Payload() []byte {
	return i[timestampLengthInBytes:]
}

// String-encoded representation that can be passed through Parse()
func (i KSUID) String() string {
	return string(i.Append(make([]byte, 0, stringEncodedLength)))
}

// Raw byte representation of KSUID
func (i KSUID) Bytes() []byte {
	// Safe because this is by-value
	return i[:]
}

// Returns true if this is a "nil" KSUID
func (i KSUID) IsNil() bool {
	return i == Nil
}

// Get satisfies the flag.Getter interface, making it possible to use KSUIDs as
// part of of the command line options of a program.
func (i KSUID) Get() interface{} {
	return i
}

// Set satisfies the flag.Value interface, making it possible to use KSUIDs as
// part of of the command line options of a program.
func (i *KSUID) Set(s string) error {
	return i.UnmarshalText([]byte(s))
}

func (i KSUID) MarshalText() ([]byte, error) {
	return []byte(i.String()), nil
}

func (i KSUID) MarshalBinary() ([]byte, error) {
	return i.Bytes(), nil
}

func (i *KSUID) UnmarshalText(b []byte) error {
	id, err := Parse(string(b))
	if err != nil {
		return err
	}
	*i = id
	return nil
}

func (i *KSUID) UnmarshalBinary(b []byte) error {
	id, err := FromBytes(b)
	if err != nil {
		return err
	}
	*i = id
	return nil
}

// Value converts the KSUID into a SQL driver value which can be used to
// directly use the KSUID as parameter to a SQL query.
func (i KSUID) Value() (driver.Value, error) {
	return i.String(), nil
}

// Scan implements the sql.Scanner interface. It supports converting from
// string, []byte, or nil into a KSUID value. Attempting to convert from
// another type will return an error.
func (i *KSUID) Scan(src interface{}) error {
	switch v := src.(type) {
	case nil:
		return i.scan(nil)
	case []byte:
		return i.scan(v)
	case string:
		return i.scan([]byte(v))
	default:
		return fmt.Errorf("Scan: unable to scan type %T into KSUID", v)
	}
}

func (i *KSUID) scan(b []byte) error {
	switch len(b) {
	case 0:
		*i = Nil
		return nil
	case byteLength:
		return i.UnmarshalBinary(b)
	case stringEncodedLength:
		return i.UnmarshalText(b)
	default:
		return errSize
	}
}

// Decodes a string-encoded representation of a KSUID object
func Parse(s string) (KSUID, error) {
	if len(s) != stringEncodedLength {
		return Nil, errStrSize
	}

	src := [stringEncodedLength]byte{}
	dst := [byteLength]byte{}

	copy(src[:], s[:])
	decoded := appendDecodeBase62(dst[:0], src[:])

	if pad := byteLength - len(decoded); pad > 0 {
		zeroes := [byteLength]byte{}
		copy(dst[pad:], dst[:])
		copy(dst[:], zeroes[:pad])
	}

	return FromBytes(dst[:])
}

func timeToCorrectedUTCTimestamp(t time.Time) uint32 {
	return uint32(t.Unix() - epochStamp)
}

func correctedUTCTimestampToTime(ts uint32) time.Time {
	return time.Unix(int64(ts)+epochStamp, 0)
}

// Generates a new KSUID. In the strange case that random bytes
// can't be read, it will panic.
func New() KSUID {
	ksuid, err := NewRandom()
	if err != nil {
		panic(fmt.Sprintf("Couldn't generate KSUID, inconceivable! error: %v", err))
	}
	return ksuid
}

// Generates a new KSUID
func NewRandom() (KSUID, error) {
	payload := payloadPool.Get().(*payload)

	_, err := io.ReadFull(rander, (*payload)[:])
	if err != nil {
		return Nil, err
	}

	ksuid, err := FromParts(time.Now(), (*payload)[:])
	if err != nil {
		return Nil, err
	}

	payloadPool.Put(payload)
	return ksuid, nil
}

// Constructs a KSUID from constituent parts
func FromParts(t time.Time, payload []byte) (KSUID, error) {
	if len(payload) != payloadLengthInBytes {
		return Nil, errPayloadSize
	}

	var ksuid KSUID

	ts := timeToCorrectedUTCTimestamp(t)
	binary.BigEndian.PutUint32(ksuid[:timestampLengthInBytes], ts)

	copy(ksuid[timestampLengthInBytes:], payload)

	return ksuid, nil
}

// Constructs a KSUID from a 20-byte binary representation
func FromBytes(b []byte) (KSUID, error) {
	var ksuid KSUID

	if len(b) != byteLength {
		return Nil, errSize
	}

	copy(ksuid[:], b)
	return ksuid, nil
}

// Sets the global source of random bytes for KSUID generation. This
// should probably only be set once globally. While this is technically
// thread-safe as in it won't cause corruption, there's no guarantee
// on ordering.
func SetRand(r io.Reader) {
	if r == nil {
		rander = rand.Reader
		return
	}
	rander = r
}

// Implements comparison for KSUID type
func Compare(a, b KSUID) int {
	return bytes.Compare(a[:], b[:])
}

// This type and the associated memory pool are used to allocate buffers to
// generate KSUIDs by reading the random source.
type payload [payloadLengthInBytes]byte

var payloadPool = sync.Pool{
	New: func() interface{} { return new(payload) },
}
