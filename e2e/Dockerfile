# The runtime image for the e2e test. You build the function binary first, so
# this needs no BuildKit and no Go inside the image.
FROM gcr.io/distroless/static-debian12:nonroot
COPY function /function
USER nonroot:nonroot
ENTRYPOINT ["/function"]
