# syntax=docker/dockerfile:1
FROM gcr.io/distroless/static-debian13:nonroot
ARG TARGETARCH
ARG VERSION
ARG COMMIT
WORKDIR /
COPY dist/feedback_linux_${TARGETARCH}*/feedback /feedback
# Fail if an image would ship a snapshot or a different commit under a release tag.
RUN ["/feedback", "verify-build"]
LABEL org.opencontainers.image.source="https://github.com/NerdsWhoFish/feedback" \
      org.opencontainers.image.licenses="Apache-2.0"
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/feedback"]
