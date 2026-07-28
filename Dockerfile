# Start by building the application.
FROM golang:1.26-bookworm AS build

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    pkg-config \
    wget \
    ca-certificates \
    libssl-dev \
    protobuf-compiler \
    libprotobuf-dev \
    && rm -rf /var/lib/apt/lists/*

# build libsodium (dep of libzmq)
WORKDIR /build
RUN wget https://github.com/jedisct1/libsodium/releases/download/1.0.19-RELEASE/libsodium-1.0.19.tar.gz
RUN tar -xzvf libsodium-1.0.19.tar.gz
WORKDIR /build/libsodium-stable
RUN ./configure --disable-shared --enable-static
RUN make -j$(nproc)
RUN make install

# build libzmq (dep of zmq datastore)
WORKDIR /build
RUN wget https://github.com/zeromq/libzmq/releases/download/v4.3.4/zeromq-4.3.4.tar.gz
RUN tar -xvf zeromq-4.3.4.tar.gz
WORKDIR /build/zeromq-4.3.4
RUN ./configure --enable-static --disable-shared --disable-Werror
RUN make -j$(nproc)
RUN make install

# Install rdkafka dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    librdkafka-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /go/src/fleet-telemetry

COPY . .
ENV CGO_ENABLED=1
ENV CGO_LDFLAGS="-lstdc++ -Wl,-rpath,/usr/local/lib"
ENV PKG_CONFIG_PATH=/usr/local/lib/pkgconfig

RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.1
RUN make generate-protos
RUN make > /tmp/build.log 2>&1 || { \
    status=$?; \
    echo "=== make failed; final build output ==="; \
    tail -n 250 /tmp/build.log; \
    exit "$status"; \
    }

# hadolint ignore=DL3006
FROM gcr.io/distroless/cc-debian12:nonroot
WORKDIR /
COPY --from=build /go/bin/fleet-telemetry /

CMD ["/fleet-telemetry", "-config", "/etc/fleet-telemetry/config.json"]
