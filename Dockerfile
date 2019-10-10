FROM golang:latest as build

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

# Copy the source from the current directory to the Working Directory inside the container
COPY . .

# Build the Go app
RUN go build -o mcp .

FROM alpine

COPY --from=build /app/mcp /app/mcp 

# Expose port 18848 to the outside world
EXPOSE 18848

# Command to run the executable
CMD ["/app/mcp"]