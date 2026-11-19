"""Downloads the ONNX NSFW image-classification model used by OnnxModel.

Dev-only: needs the "fetch" dependency group (huggingface_hub), which is
*not* installed by a plain `uv sync` — it would otherwise drag a Hub client
into the runtime image for something only needed once, at build/dev time.

Usage (from inference/image/):
    uv sync --group fetch
    uv run --group fetch python scripts/fetch_model.py

Downloads into inference/image/models/ (gitignored). Point the app at the
result with:
    MODEL_BACKEND=onnx
    MODEL_PATH=models/onnx/model_quantized.onnx

Repo choice: "onnx-community/Falconsai-nsfw_image_detection" (the id one
would guess first) does not exist on the Hub. Per the fallback plan, this
targets an ONNX export of Falconsai/nsfw_image_detection (the standard ViT
NSFW classifier, id2label {0: "normal", 1: "nsfw"}) found by searching the
Hub: onnx-community/nsfw_image_detection-ONNX, a transformers.js-published
ONNX export of that exact base model.
"""

from pathlib import Path

from huggingface_hub import hf_hub_download

REPO_ID = "onnx-community/nsfw_image_detection-ONNX"
MODEL_FILENAME = "onnx/model_quantized.onnx"


def main() -> None:
    target_dir = Path(__file__).resolve().parent.parent / "models"
    target_dir.mkdir(parents=True, exist_ok=True)

    model_path = hf_hub_download(
        repo_id=REPO_ID, filename=MODEL_FILENAME, local_dir=str(target_dir)
    )

    print(f"model: {model_path}")


if __name__ == "__main__":
    main()
