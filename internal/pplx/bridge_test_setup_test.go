package pplx

import "os"

func init() { os.Setenv("PPLX_NO_BRIDGE", "1") }
