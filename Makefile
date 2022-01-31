build:
	go build -o bin/booty cmd/main.go

run:
	go run cmd/main.go

image:
	docker build -t jeefy/booty .

image-push:
	docker push jeefy/booty