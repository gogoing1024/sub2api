import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

const currentDir = dirname(fileURLToPath(import.meta.url));
const groupsViewSource = readFileSync(resolve(currentDir, "../GroupsView.vue"), "utf8");

describe("groups Kiro cache emulation modes", () => {
  it("exposes uniform and independent ratio controls for create and edit", () => {
    expect(groupsViewSource).toContain("setCreateKiroCacheMode('uniform')");
    expect(groupsViewSource).toContain("setCreateKiroCacheMode('independent')");
    expect(groupsViewSource).toContain("setEditKiroCacheMode('uniform')");
    expect(groupsViewSource).toContain("setEditKiroCacheMode('independent')");
    expect(groupsViewSource).toContain("kiro_cache_creation_emulation_ratio");
    expect(groupsViewSource).toContain("kiro_cache_read_emulation_ratio");
    expect(groupsViewSource.match(/<KiroCacheRatioField/g)).toHaveLength(6);
    expect(groupsViewSource).toContain(
      ":aria-pressed=\"createForm.kiro_cache_emulation_mode === 'uniform'\"",
    );
    expect(groupsViewSource).toContain(
      ":aria-pressed=\"editForm.kiro_cache_emulation_mode === 'independent'\"",
    );
  });

  it("inherits the uniform ratio when switching to independent mode", () => {
    expect(groupsViewSource).toContain(
      "createForm.kiro_cache_creation_emulation_ratio = createForm.kiro_cache_emulation_ratio",
    );
    expect(groupsViewSource).toContain(
      "editForm.kiro_cache_read_emulation_ratio = editForm.kiro_cache_emulation_ratio",
    );
  });
});

describe("groups Kiro cache source mode", () => {
  it("exposes emulation_only and upstream_first controls for create and edit", () => {
    expect(groupsViewSource).toContain(
      ":aria-pressed=\"createForm.kiro_cache_source_mode === 'emulation_only'\"",
    );
    expect(groupsViewSource).toContain(
      ":aria-pressed=\"createForm.kiro_cache_source_mode === 'upstream_first'\"",
    );
    expect(groupsViewSource).toContain(
      ":aria-pressed=\"editForm.kiro_cache_source_mode === 'emulation_only'\"",
    );
    expect(groupsViewSource).toContain(
      ":aria-pressed=\"editForm.kiro_cache_source_mode === 'upstream_first'\"",
    );
    expect(groupsViewSource).toContain("admin.groups.kiroCache.sourceModeHint");
  });

  it("defaults to emulation_only and backfills the edit form from the group", () => {
    // 两处表单初值（create / edit）
    expect(
      groupsViewSource.match(
        /kiro_cache_source_mode: "emulation_only" as "emulation_only" \| "upstream_first"/g,
      ),
    ).toHaveLength(2);
    expect(groupsViewSource).toContain(
      'createForm.kiro_cache_source_mode = "emulation_only"',
    );
    expect(groupsViewSource).toContain(
      'editForm.kiro_cache_source_mode = group.kiro_cache_source_mode === "upstream_first"',
    );
  });

  it("forces emulation_only for non-kiro platforms on create and update", () => {
    expect(groupsViewSource).toContain(
      'requestData.kiro_cache_source_mode = "emulation_only"',
    );
    expect(groupsViewSource).toContain(
      'payload.kiro_cache_source_mode = "emulation_only"',
    );
  });
});
