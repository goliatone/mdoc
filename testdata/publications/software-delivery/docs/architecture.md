# Architecture

## Components

The gateway calls the application service. The application service owns state changes.

| Component | Responsibility |
|---|---|
| Gateway | Request routing |
| Service | Business rules |
