package service

import "errors"

var (
	ErrUnprocessable = errors.New("unprocessable")
	ErrUnavailable   = errors.New("unavailable")
	ErrGone          = errors.New("gone")
)
