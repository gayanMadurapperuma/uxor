// Package config loads .codegraph.yaml and resolves the project a command
// operates on. Every project is isolated: its root, data directory and
// database are derived from one resolved root and nothing else.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// FileName is the optional per-project config file at the project root.
const FileName = ".codegraph.yaml"

type Budgets struct {
	QueryTokens   int `yaml:"query_tokens"`
	HookTokens    int `yaml:"hook_tokens"`
	HookTop       int `yaml:"hook_top"`
	HookTimeoutMS int `yaml:"hook_timeout_ms"`
}

type Retrieve struct {
	Seeds     int `yaml:"seeds"`
	Depth     int `yaml:"depth"`
	MaxPerHop int `yaml:"max_per_hop"`
	HubFanIn  int `yaml:"hub_fanin"`
}

type ResolveConfig struct {
	// PythonRoots are extra source roots (relative to the project root) that
	// Python absolute imports are resolved against, in addition to the root,
	// src/ and directories holding pyproject.toml/setup.py/setup.cfg.
	PythonRoots []string `yaml:"python_roots"`
}

type Config struct {
	Include       []string      `yaml:"include"`
	Exclude       []string      `yaml:"exclude"` // added to DefaultExcludes
	Languages     []string      `yaml:"languages"`
	MaxFileBytes  int64         `yaml:"max_file_bytes"`
	IncludeNested bool          `yaml:"include_nested"`
	DBPath        string        `yaml:"db_path"` // relative to the root
	Budgets       Budgets       `yaml:"budgets"`
	Retrieve      Retrieve      `yaml:"retrieve"`
	Resolve       ResolveConfig `yaml:"resolve"`
}

// DefaultExcludes are always applied, before user excludes.
var DefaultExcludes = []string{
	"**/.git/**", "**/node_modules/**", "**/vendor/**", "**/dist/**", "**/build/**",
	"**/.venv/**", "**/venv/**", "**/__pycache__/**", "**/.codegraph/**", "**/.next/**",
	"**/.tox/**", "**/.mypy_cache/**", "**/coverage/**",
	"**/*.min.js", "**/*.bundle.js", "**/*.pb.go", "**/*_pb2.py", "**/*_pb2_grpc.py",
	"**/*.generated.*", "**/*.gen.go",
	"**/package-lock.json", "**/yarn.lock", "**/pnpm-lock.yaml", "**/go.sum",
	"**/poetry.lock", "**/Pipfile.lock", "**/uv.lock",
}

// Default returns the configuration used when no file is present.
func Default() *Config {
	return &Config{
		Include:      []string{"**/*"},
		Languages:    []string{"go", "python", "typescript", "javascript"},
		MaxFileBytes: 1 << 20,
		Budgets:      Budgets{QueryTokens: 1000, HookTokens: 500, HookTop: 5, HookTimeoutMS: 250},
		Retrieve:     Retrieve{Seeds: 20, Depth: 2, MaxPerHop: 50, HubFanIn: 100},
	}
}

// Load reads <root>/.codegraph.yaml over the defaults. A missing file is not
// an error.
func Load(root string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(filepath.Join(root, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return c, c.validate()
}

func (c *Config) validate() error {
	for _, g := range append(append([]string{}, c.Include...), c.Exclude...) {
		if !doublestar.ValidatePattern(g) {
			return fmt.Errorf("%s: invalid glob %q", FileName, g)
		}
	}
	if len(c.Include) == 0 {
		c.Include = []string{"**/*"}
	}
	if c.MaxFileBytes <= 0 {
		c.MaxFileBytes = 1 << 20
	}
	for _, r := range c.Resolve.PythonRoots {
		if filepath.IsAbs(r) || r == ".." || strings.HasPrefix(filepath.ToSlash(r), "../") {
			return fmt.Errorf("%s: resolve.python_roots must be inside the project: %q", FileName, r)
		}
	}
	if c.DBPath != "" && filepath.IsAbs(c.DBPath) {
		return fmt.Errorf("%s: db_path must be relative to the project root", FileName)
	}
	d := Default()
	if c.Budgets.QueryTokens <= 0 {
		c.Budgets.QueryTokens = d.Budgets.QueryTokens
	}
	if c.Budgets.HookTokens <= 0 {
		c.Budgets.HookTokens = d.Budgets.HookTokens
	}
	if c.Budgets.HookTop <= 0 {
		c.Budgets.HookTop = d.Budgets.HookTop
	}
	if c.Budgets.HookTimeoutMS <= 0 {
		c.Budgets.HookTimeoutMS = d.Budgets.HookTimeoutMS
	}
	if c.Retrieve.Seeds <= 0 {
		c.Retrieve.Seeds = d.Retrieve.Seeds
	}
	if c.Retrieve.Depth <= 0 {
		c.Retrieve.Depth = d.Retrieve.Depth
	}
	if c.Retrieve.MaxPerHop <= 0 {
		c.Retrieve.MaxPerHop = d.Retrieve.MaxPerHop
	}
	if c.Retrieve.HubFanIn <= 0 {
		c.Retrieve.HubFanIn = d.Retrieve.HubFanIn
	}
	return nil
}

// LanguageEnabled reports whether lang is listed in Languages.
func (c *Config) LanguageEnabled(lang string) bool {
	return slices.Contains(c.Languages, lang)
}
