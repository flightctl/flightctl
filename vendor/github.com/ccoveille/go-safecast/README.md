# 🪄 go-safecast: safe numbers conversion

[![Go Report Card](https://goreportcard.com/badge/github.com/ccoveille/go-safecast)](https://goreportcard.com/report/github.com/ccoveille/go-safecast)
[![GoDoc](https://godoc.org/github.com/ccoVeille/go-safecast?status.svg)](https://godoc.org/github.com/ccoVeille/go-safecast)
[![codecov](https://codecov.io/gh/ccoVeille/go-safecast/graph/badge.svg?token=VW0VO503U6)](https://codecov.io/gh/ccoVeille/go-safecast)
[![Code Climate](https://codeclimate.com/github/ccoVeille/go-safecast.png)](https://codeclimate.com/github/ccoVeille/go-safecast)

## Project purpose

In Go, integer type conversion can lead to a silent and unexpected behavior and errors if not handled carefully.

This package is made to help to convert any number to another, and report an error when if there would be a [loss or overflow in the conversion](#conversion-overflows)

## Usage

```go
package main

import (
  "fmt"
  "math"

  "github.com/ccoveille/go-safecast"
)

func main() {

  // when there is no overflow
  //
  fmt.Println(safecast.ToInt8(float64(42)))
  // Output: 42, nil
  fmt.Println(safecast.ToInt8(int64(-1)))
  // Output: -1, nil

  // when there is an overflow
  //
  fmt.Println(safecast.ToInt8(float64(20000)))
  // Output: 0 conversion issue: 20000 is greater than 127
  fmt.Println(safecast.ToUint8(int64(-1)))
  // Output: 0 conversion issue: -1 is negative
  fmt.Println(safecast.ToInt16(int32(40000)))
  // Output: 0 conversion issue: 40000 is greater than 32767
  fmt.Println(safecast.ToUint16(int64(-1)))
  // Output: 0 conversion issue: -1 is negative
  fmt.Println(safecast.ToInt32(math.MaxUint32 + 1))
  // Output: 0 conversion issue: 4294967296 is greater than 2147483647
  fmt.Println(safecast.ToUint32(int64(-1)))
  // Output: 0 conversion issue: -1 is negative
  fmt.Println(safecast.ToInt64(uint64(math.MaxInt64) + 1))
  // Output: 0 conversion issue: 9223372036854775808 is greater than 9223372036854775807
  fmt.Println(safecast.ToUint64(int8(-1)))
  // Output: 0 conversion issue: -1 is negative
  fmt.Println(safecast.ToInt(uint64(math.MaxInt) + 1))
  // Output: 0 conversion issue: 9223372036854775808 is greater than 9223372036854775807
  fmt.Println(safecast.ToUint(int8(-1)))
  // Output: 0 conversion issue: -1 is negative
}
```

[Go Playground](https://go.dev/play/p/VCrv1aLJjMQ)

## Conversion overflows

Issues can happen when converting between signed and unsigned integers, or when converting to a smaller integer type.

```go
package main

import "fmt"

func main() {
  var a int64
  a = 42
  b := uint8(a)
  fmt.Println(b) // 42

  a = 255 // this is the math.MaxUint8
  b = uint8(a)
  fmt.Println(b) // 255

  a = 255 + 1
  b = uint8(a)
  fmt.Println(b) // 0 conversion overflow

  a = -1
  b = uint8(a)
  fmt.Println(b) // 255 conversion overflow
}
```

[Go Playground](https://go.dev/play/p/DHfNUcZBvVn)

## Motivation

The gosec project raised this to my attention when the gosec [G115 rule was added](https://github.com/securego/gosec/pull/1149)

> G115: Potential overflow when converting between integer types.

This issue was way more complex than expected, and required multiple fixes.

[CWE-190](https://cwe.mitre.org/data/definitions/190.html) explains in detail.

But to sum it up, you can face:

- infinite loop
- access to wrong resource by id
- grant access to someone who exhausted their quota

The gosec G115 will now report issues in a lot of project.

## Alternatives

Some libraries existed, but they were not able to cover all the use cases.

- [github.com/rung/go-safecast](https://github.com/rung/go-safecast):
  Unmaintained, not architecture agnostic, do not support `uint` -> `int` conversion

- [github.com/cybergarage/go-safecast](https://github.com/cybergarage/go-safecast)
  Work with pointer like `json.Marshall`
