package picker

import "encoding/base64"

func stdEncode(b []byte) string         { return base64.StdEncoding.EncodeToString(b) }
func stdDecode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
