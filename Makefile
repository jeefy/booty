build:
	go build -o bin/booty *.go

run:
	go run *.go

image:
	docker build -t jeefy/booty .

image-push:
	docker push jeefy/booty