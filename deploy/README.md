# Deployment Layout

This folder contains platform-specific deployment adapters and templates.

## Why this layout

- Keep root focused on core DoH implementation.
- Keep provider-specific files isolated under `deploy/`.
- Preserve root-level files that platforms require at repository root.

## Platform mapping

- Vercel
  - Root-level requirements: `api/*`, `vercel.json`
  - Shared runtime logic: `platform`
- Netlify
  - Root-level requirement: `netlify.toml`
  - Functions source: `deploy/netlify/functions/*`
- Cloudflare Workers
  - Worker and Wrangler template: `deploy/cloudflare/*`
- Railway
  - Root-level requirement: `railway.json`

## Shared bootstrap

- `relay` builds the app from environment variables.
- All serverless adapters call the same bootstrap to avoid behavior drift.
