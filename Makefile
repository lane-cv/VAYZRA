.PHONY: test-go test-web verify
test-go:
	go test ./...
test-web:
	pnpm test && pnpm typecheck && pnpm build
verify: test-go test-web
