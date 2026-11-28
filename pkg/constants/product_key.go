package constants

const (
	ProductKeyEnvelopeAlgorithm     = "RSA-OAEP-SHA256"
	ProductKeyEnvelopeFieldCount    = 5
	ProductKeyEnvelopeMaxBytes      = 32 * 1024
	ProductContentKeyBytes          = 32
	ProductKeyCiphertextBytes       = ProductSessionRSAKeyBits / 8
	ProductKeyCiphertextBase64Bytes = 4 * ((ProductKeyCiphertextBytes + 2) / 3)
)
