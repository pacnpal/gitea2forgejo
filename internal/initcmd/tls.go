package initcmd

import "crypto/tls"

func tlsInsecureConfig() *tls.Config { return &tls.Config{InsecureSkipVerify: true} }
