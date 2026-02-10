FROM informalsystems/hermes:1.10.0 AS hermes-builder

# Final image with hermes binary on debian-slim
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

RUN useradd -m hermes

COPY --from=hermes-builder /usr/bin/hermes /usr/local/bin/hermes

EXPOSE 3031

USER hermes

ENTRYPOINT ["hermes"]
