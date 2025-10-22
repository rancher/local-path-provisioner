/*
Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package util

import (
	"errors"
	"time"
)

var _ error = &DelayRetryError{}

type DelayRetryError struct {
	msg      string
	tryAfter time.Duration
}

func (e *DelayRetryError) Error() string {
	return e.msg
}

func (e *DelayRetryError) TryAfter() time.Duration {
	return e.tryAfter
}

func IsDelayRetryError(err error) bool {
	var dre *DelayRetryError
	return errors.As(err, &dre)
}

func NewDelayRetryError(msg string, tryAfter time.Duration) *DelayRetryError {
	return &DelayRetryError{
		msg:      msg,
		tryAfter: tryAfter,
	}
}
