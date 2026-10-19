# Redacted plex.tv response shapes

These small JSON documents preserve the structure and HTTP status of
read-only responses captured from the owner's Plex reference server on
2026-10-19. They are structural fixtures, not raw payloads: response fields
containing tokens, IDs, account details, addresses, hostnames, or hashes are
replaced with labeled placeholders. The fixture trees preserve the captured collection shape and element/attribute
names. No credential or raw
personal response body is stored here.

The PMS server-scoped API responses are XML; the fixtures encode their XML
element/attribute tree as JSON so they can document field names without
retaining attribute values. `plex_resources.json`, `plex_user.json`, and
`media_providers.json` show their respective JSON/XML tree shape as returned
by the live endpoints.
