# Follow-up

- Expose actual Compose/Podman stack runtime status instead of the current `saved` metadata status. The stack list explicitly labels this limitation.
- Verify `podman-compose` successful exit implies the intended workload was created and started; the installed provider has previously printed pull errors while returning exit code 0. Do not equate operation success with running containers until this check exists.
