package pathscope

import (
	"path/filepath"
	"strings"
)

// lang is one comment-table language: the comment and string forms its
// lexer must handle, and the comment prefixes that are directives. The
// table is compiled in, not config: an editable directive list would let an
// actor loosen its own check.
type lang struct {
	name string

	// Line comments.
	hashAny       bool // '#' starts a comment anywhere outside a string
	hashWord      bool // '#' starts a comment only at the start of a word
	slashLine     bool // '//' starts a comment
	dashLine      bool // '--' starts a comment
	dashSymbol    bool // '--' is a comment only when not followed by a symbol char
	dashBracket   bool // '--[' opens a Lua long comment
	hashBangFirst bool // '#!' is a directive on the first line only

	// Block comments.
	blockSlash bool // '/*' opens a block comment
	blockNest  bool // '/* */' block comments nest
	blockBrace bool // '{-' opens a block comment
	braceNest  bool // '{- -}' block comments nest

	// Strings.
	single      bool // ' opens a string
	sqEsc       bool // a backslash escapes inside '...'
	sqDouble    bool // '' doubles inside '...'
	double      bool // " opens a string
	dqEsc       bool // a backslash escapes inside "..."
	triple      bool // ''' and """ open multi-line strings
	backtick    bool // ` opens a template literal
	dollarQuote bool // $' and $" open strings
	dollarTag   bool // $tag$ opens a dollar-quoted string
	verbatim    bool // @" opens a verbatim string
	pyPrefix    bool // an r/b/f/u prefix opens a string
	rsPrefix    bool // an r/b/c prefix opens a string
	swiftHash   bool // '#'s before " open a raw string
	dartPrefix  bool // r before a quote opens a raw string
	longBracket bool // [[ and [=[ open long strings
	heredoc     bool // <<WORD heredocs
	yamlBlock   bool // | and > block scalars
	arith       bool // (( )) arithmetic, where << is a shift

	directives []string
}

// hasCommentMarker reports whether the language lexes any comment form.
func (l *lang) hasCommentMarker() bool {
	return l.hashAny || l.hashWord || l.slashLine || l.dashLine ||
		l.dashSymbol || l.blockSlash || l.blockBrace || l.dashBracket
}

// langFor returns the table language for path, or nil when the path's
// extension is not in the table. The lookup lowercases the extension, so
// .PY is Python; basenames are not consulted, so Makefile stays out.
func langFor(path string) *lang {
	return extTable[strings.ToLower(filepath.Ext(path))]
}

// extTable keys every table language by its lowercased extension. .go is
// not here: it keeps the go/scanner rule.
var extTable = map[string]*lang{
	".py": pyLang, ".pyi": pyLang,
	".sh": shLang, ".bash": shLang, ".zsh": shLang,
	".yaml": yamlLang, ".yml": yamlLang,
	".toml": tomlLang,
	".r":    rLang,
	".js":   jsLang, ".jsx": jsLang, ".mjs": jsLang, ".cjs": jsLang, ".ts": jsLang, ".tsx": jsLang,
	".java": javaLang, ".kt": javaLang, ".kts": javaLang, ".scala": javaLang,
	".cs":    csLang,
	".swift": swiftLang,
	".dart":  dartLang,
	".c":     cLang, ".h": cLang, ".cc": cLang, ".cpp": cLang, ".hpp": cLang,
	".rs":  rsLang,
	".sql": sqlLang,
	".lua": luaLang,
	".hs":  hsLang,
}

// The directive tables, one per language family: the comment prefixes a
// tool reads. A language left out of this list has none.
var (
	pyDirectives = []string{
		"#!", "# -*- coding", "# coding:", "# type:", "# noqa", "# pyright:",
		"# pylint:", "# ruff:", "# fmt:", "# isort:", "# mypy:", "# pragma:",
		"# flake8:",
	}
	shDirectives   = []string{"#!", "# shellcheck", "#shfmt:", "# shfmt:"}
	yamlDirectives = []string{"# yamllint", "# yaml-language-server:", "# yamlfmt"}
	tomlDirectives = []string{"#:schema", "# taplo:"}
	jsDirectives   = []string{
		"// @ts-", "/// <reference", "// eslint-", "/* eslint-",
		"// prettier-ignore", "// biome-ignore", "// deno-lint-ignore",
		"// istanbul ignore", "// @flow", "//#region",
		"// @vitest-environment", "// @jest-environment", "// webpack",
	}
	jvmDirectives = []string{
		"//noinspection", "// language=", "// swiftlint:", "// ignore:",
		"// ignore_for_file:", "// dart format", "// coverage:ignore",
		"// region", "// endregion", "// ReSharper",
	}
	cDirectives = []string{
		"// NOLINT", "//NOLINT", "// clang-format", "/* clang-format",
		"// IWYU pragma:", "// cppcheck-suppress", "// clang-tidy",
		"// ReSharper",
	}
	sqlDirectives = []string{"-- noqa", "-- sqlfluff:", "-- name:", "-- +migrate", "/*!"}
	luaDirectives = []string{"---@", "-- luacheck:", "-- stylua:", "-- selene:"}
	hsDirectives  = []string{"{-#"}
)

// The table's languages, one var per language so a mutation can delete one
// table entry and nothing else.
var (
	pyLang = &lang{
		name: "py", hashAny: true, hashBangFirst: true,
		single: true, sqEsc: true, double: true, dqEsc: true, triple: true,
		pyPrefix: true, directives: pyDirectives,
	}
	shLang = &lang{
		name: "sh", hashWord: true, hashBangFirst: true,
		single: true, double: true, dqEsc: true,
		dollarQuote: true, heredoc: true, arith: true, directives: shDirectives,
	}
	yamlLang = &lang{
		name: "yaml", hashWord: true,
		single: true, sqDouble: true, double: true, dqEsc: true,
		yamlBlock: true, directives: yamlDirectives,
	}
	tomlLang = &lang{
		name: "toml", hashAny: true,
		single: true, sqEsc: true, double: true, dqEsc: true, triple: true,
		directives: tomlDirectives,
	}
	rLang = &lang{
		name: "r", hashAny: true,
		single: true, sqEsc: true, double: true, dqEsc: true,
	}
	jsLang = &lang{
		name: "js", slashLine: true, blockSlash: true,
		single: true, sqEsc: true, double: true, dqEsc: true, backtick: true,
		directives: jsDirectives,
	}
	javaLang = &lang{
		name: "java", slashLine: true, blockSlash: true,
		single: true, sqEsc: true, double: true, dqEsc: true, triple: true,
		directives: jvmDirectives,
	}
	csLang = &lang{
		name: "cs", slashLine: true, blockSlash: true,
		single: true, sqEsc: true, double: true, dqEsc: true, triple: true,
		verbatim: true, directives: jvmDirectives,
	}
	swiftLang = &lang{
		name: "swift", slashLine: true, blockSlash: true,
		double: true, dqEsc: true, triple: true, swiftHash: true,
		directives: jvmDirectives,
	}
	dartLang = &lang{
		name: "dart", slashLine: true, blockSlash: true,
		single: true, sqEsc: true, double: true, dqEsc: true, triple: true,
		dartPrefix: true, directives: jvmDirectives,
	}
	cLang = &lang{
		name: "c", slashLine: true, blockSlash: true,
		single: true, sqEsc: true, double: true, dqEsc: true,
		directives: cDirectives,
	}
	rsLang = &lang{
		name: "rs", slashLine: true, blockSlash: true, blockNest: true,
		double: true, dqEsc: true, rsPrefix: true,
	}
	sqlLang = &lang{
		name: "sql", dashLine: true, blockSlash: true,
		single: true, sqDouble: true, dollarTag: true, directives: sqlDirectives,
	}
	luaLang = &lang{
		name: "lua", dashLine: true, dashBracket: true,
		single: true, sqEsc: true, double: true, dqEsc: true, longBracket: true,
		directives: luaDirectives,
	}
	hsLang = &lang{
		name: "hs", dashSymbol: true, blockBrace: true, braceNest: true,
		double: true, dqEsc: true, directives: hsDirectives,
	}
)
