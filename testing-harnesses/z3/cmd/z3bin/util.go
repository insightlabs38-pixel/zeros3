package main

import "time"

func nowNS() int64 { return time.Now().UnixNano() }
