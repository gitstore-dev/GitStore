function(ctx) {
  identity: {
    id: ctx.identity.id,
    schema_id: ctx.identity.schema_id,
    traits: ctx.identity.traits,
    metadata_admin: std.get(ctx.identity, 'metadata_admin', {}),
    created_at: std.get(ctx.identity, 'created_at', null),
  },
}
