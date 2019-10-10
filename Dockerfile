FROM golang:1.13-alpine as builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o mcp .

FROM alpine
COPY --from=builder /app/mcp /app/mcp
EXPOSE 9901
ENTRYPOINT ["/app/mcp"]
