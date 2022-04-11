FROM golang:1.18rc1-alpine3.15

WORKDIR /app

# pre-copy/cache go.mod for pre-downloading dependencies and only redownloading them in subsequent builds if they change
COPY go.mod go.sum ./

RUN go mod download && go mod verify

COPY . .

RUN go build -o main .

EXPOSE 2113

CMD ["./main"]

