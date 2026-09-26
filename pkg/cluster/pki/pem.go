package pki

import "encoding/pem"

func encodePEM(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func decodePEM(data []byte, typ string) (der, rest []byte) {
	for rest = data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, nil
		}
		if block.Type == typ {
			return block.Bytes, rest
		}
	}
}
