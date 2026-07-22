# go++ VS Code extension

Syntax highlighting and language-server support for the [go++](../../README.md) language (`.gopp` files).

## What works

- **Syntax highlighting** via a TextMate grammar (`syntaxes/gopp.tmLanguage.json`): comments, strings, runes, numbers, keywords (`match`, `loop`, `fn`, `enum`, `behavior`, `impl`, `comptime`, …), primitive types, variant constructors, and operators such as `->`, `:=`, `<-`, `?`.
- **Language features** powered by the compiler's built-in LSP server (`gopp lsp`, over stdio):
  - diagnostics (errors/warnings as you type)
  - hover
  - go to definition
  - completion
  - document symbols

## Prerequisites

The extension spawns the `gopp` compiler binary, so `gopp` must be on your `PATH` (or configure its location, see below). The extension itself needs its Node dependencies installed once:

```sh
cd editors/vscode
npm install
```

## Settings

| Setting        | Default | Description                                   |
| -------------- | ------- | --------------------------------------------- |
| `gopp.lspPath` | `gopp`  | Path to the gopp binary used for `gopp lsp`. |

## Install

Symlink the folder into your VS Code extensions directory:

```sh
ln -s "$PWD/editors/vscode" ~/.vscode/extensions/gopp-vscode
```

or install it as an extension folder:

```sh
code --install-extension editors/vscode
```

Then reload VS Code and open a `.gopp` file.

## Packaging

To build a `.vsix` you can share or publish:

```sh
cd editors/vscode
npm install
npx @vscode/vsce package
code --install-extension gopp-vscode-0.1.0.vsix
```
