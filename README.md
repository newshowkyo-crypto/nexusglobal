# NexusGlobal MAMR System

Enterprise-grade cross-border treasury / acquiring / clearing / disbursement system.

## Quick Start

```bash
cp .env.example .env
make up
make migrate
make build
make test-short
```

## Development

```bash
make build        # Compile
make test-short   # Unit tests
make test         # Full integration tests (needs make up)
make lint         # Static analysis
make ci           # Full CI check
make redlines     # CONSTRAINTS.md redline verification
```

## Architecture

- Go 1.22+ modular monolith
- PostgreSQL 15 (pgx/v5 + sqlc)
- Redis 7 (Streams MQ)
- Zero external network dependencies in dev/CI (all mocked)

## License

Proprietary
