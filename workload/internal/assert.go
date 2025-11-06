package internal

import (
	"fmt"

	"github.com/antithesishq/antithesis-sdk-go/assert"
)

type Details map[string]any

func (d *Details) with(Details) Details {
	out := make(map[string]any)
	for k, v := range *d {
		out[k] = v
	}
	for k, v := range *d {
		out[k] = v
	}
	return out
}

func AssertAlways(condition bool, message string, details Details) bool {
	assert.Always(condition, message, details)
	return condition
}

func AssertAlwaysErrNil(err error, message string, details Details) bool {
	return AssertAlways(err == nil, message, details.with(Details{
		"error":   fmt.Sprint(err),
		"details": details,
	}))
}

func AssertSometimesErrNil(err error, message string, details Details) bool {
	assert.Sometimes(err == nil, message, details.with(Details{
		"error": err,
	}))
	return err != nil
}
