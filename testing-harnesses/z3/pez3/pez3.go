package pez3

import (
	"errors"

	"zeros3-z3/binref"
)

var ErrMalformed = errors.New("pez3: malformed")

func Parse(data []byte) (*binref.Image, error) { return nil, ErrMalformed }
