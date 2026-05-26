IMAGE      := micewriter-k8s-injector
TAG        := latest
CHART_DIR  := charts/micewriter-k8s-injector
NAMESPACE  := micewriter-system

.PHONY: build test image deploy undeploy tidy

## Fetch and resolve Go dependencies
tidy:
	go mod tidy

## Build the binary locally
build: tidy
	CGO_ENABLED=0 go build -o bin/micewriter-k8s-injector .

## Run unit tests
test:
	go test ./...

## Build the Docker image
image:
	docker build -t $(IMAGE):$(TAG) .

## Deploy the Helm chart (requires cert-manager to be installed)
deploy:
	helm upgrade --install micewriter-k8s-injector $(CHART_DIR) \
		--namespace $(NAMESPACE) --create-namespace \
		--wait

## Tear down the Helm release
undeploy:
	helm uninstall micewriter-k8s-injector --namespace $(NAMESPACE) --ignore-not-found
