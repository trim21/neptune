// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"github.com/go-playground/validator/v10"
	"github.com/trim21/errgo"
)

var configValidator = newConfigValidator()

func newConfigValidator() *validator.Validate {
	v := validator.New()

	// Reuse ParseCryptoMode so the accepted crypto modes are defined once.
	if err := v.RegisterValidation("cryptomode", validateCryptoMode); err != nil {
		panic(errgo.Wrap(err, "failed to register cryptomode validator"))
	}

	return v
}

func validateCryptoMode(fl validator.FieldLevel) bool {
	_, err := ParseCryptoMode(fl.Field().String())
	return err == nil
}

// Validate reports whether every value in the config is one the rest of the
// program can rely on. The Lua setters only convert values, they don't check
// them, so this is the single validation point for every config source.
func (c Config) Validate() error {
	if err := configValidator.Struct(c); err != nil {
		return errgo.Wrap(err, "invalid config")
	}

	return nil
}
