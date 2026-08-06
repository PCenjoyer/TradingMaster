.PHONY: format test vet build run k8s-render monitoring-render terraform-check

format:
	gofmt -w ./cmd ./internal

test:
	go test -race ./...

vet:
	go vet ./...

build:
	go build -trimpath -o bin/tradingmaster ./cmd/tradingmaster

run:
	go run ./cmd/tradingmaster -mode api

k8s-render:
	kubectl kustomize deploy/k8s

monitoring-render:
	kubectl kustomize monitoring

terraform-check:
	terraform -chdir=infra/terraform fmt -check -recursive
	terraform -chdir=infra/terraform init -backend=false
	terraform -chdir=infra/terraform validate
