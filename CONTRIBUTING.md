# Contributing to punch

Thanks for your interest in contributing!

## Getting started

```sh
git clone https://github.com/ashutoshsinghai/punch
cd punch
go build ./...
go test ./...
```

## Running locally

```sh
go run . <command>
```

## Submitting changes

1. Fork the repo and create a branch from `main`.
2. Make your changes and ensure `go build ./...` and `go test ./...` pass.
3. Open a pull request with a clear description of what you changed and why.

## Reporting bugs

Open an issue with:
- What you ran
- What you expected
- What actually happened
- Your OS and Go version

## Code style

- Run `gofmt` before committing.
- Keep changes focused — one concern per PR.
- Avoid adding dependencies unless necessary.
