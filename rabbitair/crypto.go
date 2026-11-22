package rabbitair

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrInvalidToken is returned when a token is not 16 bytes of hex.
var ErrInvalidToken = errors.New("token must be 32 hex characters (16 bytes)")

// ParseToken decodes a hex access token into its 16-byte key.
func ParseToken(token string) ([]byte, error) {
	key, err := hex.DecodeString(token)
	if err != nil || len(key) != aes.BlockSize {
		return nil, ErrInvalidToken
	}
	return key, nil
}

// encrypt pads msg with PKCS7, encrypts it with AES-128-CBC under a random
// IV and returns ciphertext followed by the IV.
func encrypt(key, msg []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	padLen := aes.BlockSize - len(msg)%aes.BlockSize
	padded := append(append([]byte{}, msg...), bytes.Repeat([]byte{byte(padLen)}, padLen)...)

	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}

	out := make([]byte, len(padded), len(padded)+aes.BlockSize)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return append(out, iv...), nil
}

// decrypt reverses encrypt: the last 16 bytes are the IV.
func decrypt(key, msg []byte) ([]byte, error) {
	if len(msg) < 2*aes.BlockSize || len(msg)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid encrypted message length %d", len(msg))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	iv := msg[len(msg)-aes.BlockSize:]
	data := make([]byte, len(msg)-aes.BlockSize)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(data, msg[:len(msg)-aes.BlockSize])

	padLen := int(data[len(data)-1])
	if padLen == 0 || padLen > aes.BlockSize {
		return nil, errors.New("invalid padding")
	}
	for _, b := range data[len(data)-padLen:] {
		if int(b) != padLen {
			return nil, errors.New("invalid padding")
		}
	}
	return data[:len(data)-padLen], nil
}
