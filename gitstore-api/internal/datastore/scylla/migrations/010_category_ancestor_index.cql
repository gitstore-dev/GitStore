-- CategoryTaxonomy ancestor closure index. One row per (ancestor, descendant)
-- pair derived from status.resolved.path, including the category itself at
-- depth 0. Derived and rebuildable: maintained by category status writes and
-- final removal, repaired by gitctl scylla-projection-repair. No CDC;

CREATE TABLE IF NOT EXISTS category_ancestor_index (
    namespace        text,
    ancestor         text,
    depth            tinyint,
    descendant       text,
    descendant_uid   uuid,
    resource_version text,
    PRIMARY KEY ((namespace, ancestor), depth, descendant)
) WITH CLUSTERING ORDER BY (depth ASC, descendant ASC)
  AND gc_grace_seconds = 864000;
