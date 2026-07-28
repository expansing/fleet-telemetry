# Start by building the application.
FROM golang:1.26-bookworm AS build

# Install build dependencies and C library development packages.  Using the
# distribution packages for libsodium and libzmq keeps their headers and
# pkg-config metadata aligned with the image architecture when buildx is used.
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    pkg-config \
    wget \
    ca-certificates \
    libssl-dev \
    protobuf-compiler \
    libprotobuf-dev \
    libsodium-dev \
    libzmq3-dev \
    libstdc++-12-dev \
    && rm -rf /var/lib/apt/lists/*

# build librdkafka (dep of kafka datastore, requires v2.3.0+)
WORKDIR /build
RUN wget https://github.com/edenhill/librdkafka/archive/refs/tags/v2.3.0.tar.gz
RUN tar -xzvf v2.3.0.tar.gz
WORKDIR /build/librdkafka-2.3.0
RUN ./configure --enable-static --disable-shared
RUN make -j$(nproc)
RUN make install

WORKDIR /go/src/fleet-telemetry

COPY . .
ENV CGO_ENABLED=1
ENV CGO_LDFLAGS="-lstdc++ -Wl,-rpath,/usr/local/lib"
ENV PKG_CONFIG_PATH=/usr/lib/pkgconfig:/usr/local/lib/pkgconfig

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
