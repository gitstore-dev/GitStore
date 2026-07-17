-- 035-git-http-auth: add push policy limit columns to repositories table.
-- Zero value means no limit enforced (FR-015).
ALTER TABLE repositories ADD max_pack_size_bytes bigint;
ALTER TABLE repositories ADD max_file_size_bytes bigint;
