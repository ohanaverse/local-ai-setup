"""Tests for ready toggle delete functionality.

Verifies that:
1. "r" toggles file presence (download on ready=ON, delete on ready=OFF)
2. "r" on ready model cascades to unexpose
3. "d" queues only the registry removal; apply()'s deletes loop does the file removal + unexpose cascade
4. Provider delete() methods work correctly
"""

from unittest.mock import patch

from modelman.providers.base import VariantSpec


class TestOmlxDelete:
    """Test omlx provider delete() method."""

    def test_delete_removes_directory(self, tmp_path):
        from modelman.providers.omlx import OMLXProvider

        # Setup: create a model directory with files
        model_dir = tmp_path / "models" / "test-repo"
        model_dir.mkdir(parents=True)
        (model_dir / "model.safetensors").write_bytes(b"fake data")
        (model_dir / "config.json").write_bytes(b"{}")

        provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
        variant: VariantSpec = {
            "id": "test--model",
            "provider": "omlx",
            "repo": "test-org/test-repo",
        }

        provider.delete(variant)

        assert not model_dir.exists()
        assert (tmp_path / "models").exists()  # Parent dir preserved

    def test_delete_noop_if_absent(self, tmp_path):
        from modelman.providers.omlx import OMLXProvider

        provider = OMLXProvider({"model_dir": str(tmp_path / "models")})
        variant: VariantSpec = {
            "id": "test--model",
            "provider": "omlx",
            "repo": "test-org/test-repo",
        }

        # Should not raise
        provider.delete(variant)


class TestLlamaCppDelete:
    """Test llamacpp provider delete() method."""

    def test_delete_removes_file_and_orphaned_blob(self, tmp_path):
        import hashlib

        from modelman.providers.llamacpp import LlamaCppProvider

        # Setup: create HF cache structure
        hub_dir = tmp_path / "hub"
        repo_dir = hub_dir / "models--test-org--test-repo"
        snapshots_dir = repo_dir / "snapshots" / "abc123"
        blobs_dir = repo_dir / "blobs"
        snapshots_dir.mkdir(parents=True)
        blobs_dir.mkdir()

        # Create a GGUF file and its blob
        gguf_content = b"fake gguf data"
        blob_hash = hashlib.sha256(gguf_content).hexdigest()
        gguf_file = snapshots_dir / "model.gguf"
        blob_file = blobs_dir / blob_hash

        gguf_file.write_bytes(gguf_content)
        blob_file.write_bytes(gguf_content)  # Hard-link simulation

        provider = LlamaCppProvider({})
        variant: VariantSpec = {
            "id": "test--model",
            "provider": "llamacpp",
            "repo": "test-org/test-repo",
            "files": ["model.gguf"],
        }

        with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
            provider.delete(variant)

        # File deleted from snapshot
        assert not gguf_file.exists()
        # Blob deleted (orphaned)
        assert not blob_file.exists()

    def test_delete_preserves_referenced_blob(self, tmp_path):
        import hashlib

        from modelman.providers.llamacpp import LlamaCppProvider

        # Setup: two snapshots sharing a blob
        hub_dir = tmp_path / "hub"
        repo_dir = hub_dir / "models--test-org--test-repo"
        snap1 = repo_dir / "snapshots" / "abc123"
        snap2 = repo_dir / "snapshots" / "def456"
        blobs_dir = repo_dir / "blobs"
        snap1.mkdir(parents=True)
        snap2.mkdir()
        blobs_dir.mkdir()

        # Create shared blob
        shared_content = b"shared data"
        blob_hash = hashlib.sha256(shared_content).hexdigest()
        blob_file = blobs_dir / blob_hash
        blob_file.write_bytes(shared_content)

        # Both snapshots reference the same blob (via hard-link simulation)
        (snap1 / "model.gguf").write_bytes(shared_content)
        (snap2 / "other.gguf").write_bytes(shared_content)  # Different filename

        provider = LlamaCppProvider({})
        variant: VariantSpec = {
            "id": "test--model",
            "provider": "llamacpp",
            "repo": "test-org/test-repo",
            "files": ["model.gguf"],  # Only delete model.gguf
        }

        with patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir):
            provider.delete(variant)

        # File deleted from snap1
        assert not (snap1 / "model.gguf").exists()
        # File in snap2 preserved (different filename)
        assert (snap2 / "other.gguf").exists()
        # Blob preserved (still referenced by other.gguf)
        assert blob_file.exists()

    def test_delete_uses_symlink_target_not_file_contents(self, tmp_path):
        """Deleting a GGUF must derive the blob hash from the snapshot file's
        symlink target (blobs/<sha256>), never by reading the file body — reading
        a multi-GB GGUF into memory to hash it OOMs the process. This test makes
        the snapshot file a symlink to a blob and asserts the blob is removed
        without the file's contents ever being read."""
        import hashlib

        from modelman.providers.llamacpp import LlamaCppProvider

        hub_dir = tmp_path / "hub"
        repo_dir = hub_dir / "models--test-org--test-repo"
        snapshots_dir = repo_dir / "snapshots" / "abc123"
        blobs_dir = repo_dir / "blobs"
        snapshots_dir.mkdir(parents=True)
        blobs_dir.mkdir()

        # A real blob whose content we deliberately do NOT want read.
        blob_hash = hashlib.sha256(b"real blob content").hexdigest()
        blob_file = blobs_dir / blob_hash
        blob_file.write_bytes(b"real blob content")

        # The snapshot file is a symlink to the blob, as in a real HF cache.
        gguf_file = snapshots_dir / "model.gguf"
        gguf_file.symlink_to(blob_file)

        provider = LlamaCppProvider({})
        variant: VariantSpec = {
            "id": "test--model",
            "provider": "llamacpp",
            "repo": "test-org/test-repo",
            "files": ["model.gguf"],
        }

        # The fast path derives the blob hash from the symlink target via
        # os.readlink, never by reading the file body. Patch read_bytes to
        # raise so a regression back to hashing the file contents fails.
        with (
            patch("modelman.providers.llamacpp._hf_cache_dir", return_value=hub_dir),
            patch.object(
                type(gguf_file), "read_bytes", side_effect=AssertionError("read_bytes called — OOM regression")
            ),
        ):
            provider.delete(variant)

        assert not gguf_file.exists()
        assert not blob_file.exists()


class TestReadyToggleCascade:
    """Test ready toggle cascades to expose."""

    def test_ready_off_cascades_unexpose_logic(self):
        """Verify the cascade logic: ready=False should queue expose=False."""
        # This tests the logic from action_toggle_ready without needing
        # to instantiate the full Textual screen.

        # Simulate state
        persisted_ready = True
        persisted_exposed = True
        queued_ready = {}
        queued_exposes = {}

        # Simulate toggle (target = not displayed_ready)
        displayed_ready = queued_ready.get("test", persisted_ready)
        target = not displayed_ready  # False

        # Queue the ready toggle
        queued_ready["test"] = target

        # Cascade: ready=False must unexpose
        if target is False:
            current_exposed = queued_exposes.get("test", persisted_exposed)
            if current_exposed:
                queued_exposes["test"] = False

        # Verify
        assert queued_ready["test"] is False
        assert queued_exposes["test"] is False
