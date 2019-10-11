export GO111MODULE ?= on
export GOPROXY ?= https://proxy.golang.org

HUB ?= gcr.io/howardjohn-istio

.PHONY: format
format:
	find . -type f -name '*.go'  -print0 | xargs -0 -r goimports -w -local github.com/howardjohn/mcp-load

.PHONY: install
install:
	go install -v

.PHONY: docker
docker:
	docker build . -t ${HUB}/mcp-load

.PHONY: push
push:
	docker push ${HUB}/mcp-load

.PHONY: deploy
deploy:
	kubectl apply -f kube
