#!/usr/bin/env python3
"""
Aurora Demo QR Code Generator
==================================================
Generate a branded QR code for the Aurora Live Demo URL.
Customise URL and output path at the bottom.

Usage:
    python3 generate_qr.py <demo_url> [output.png]
    
    # or just edit DEMO_URL below and run:
    python3 generate_qr.py
"""

import sys
import qrcode
from qrcode.image.styledpil import StyledPilImage
from qrcode.image.styles.moduledrawers.pil import RoundedModuleDrawer, GappedSquareModuleDrawer
from qrcode.image.styles.colormasks import SolidFillColorMask
from PIL import Image, ImageDraw, ImageFont, ImageFilter
import os


def make_logo(size: int = 200) -> Image.Image:
    """
    Create the Aurora brand logo (purple-to-teal gradient square with 'A')
    that will be embedded at the center of the QR code.
    """
    # Create the gradient background
    logo = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    
    # Manually draw a diagonal gradient (purple -> teal)
    # Aurora colors: purple #7F77DD, teal #5DCAA5
    for y in range(size):
        for x in range(size):
            # Diagonal interpolation factor
            t = (x + y) / (2 * size)
            t = max(0, min(1, t))
            r = int(0x7F + (0x5D - 0x7F) * t)
            g = int(0x77 + (0xCA - 0x77) * t)
            b = int(0xDD + (0xA5 - 0xDD) * t)
            logo.putpixel((x, y), (r, g, b, 255))
    
    # Round the corners
    mask = Image.new("L", (size, size), 0)
    draw = ImageDraw.Draw(mask)
    radius = size // 5
    draw.rounded_rectangle([(0, 0), (size, size)], radius=radius, fill=255)
    logo.putalpha(mask)
    
    # Add a white border (a small padding ring) around the logo
    padded_size = size + 24
    padded = Image.new("RGBA", (padded_size, padded_size), (255, 255, 255, 255))
    padded_mask = Image.new("L", (padded_size, padded_size), 0)
    padded_draw = ImageDraw.Draw(padded_mask)
    padded_draw.rounded_rectangle(
        [(0, 0), (padded_size, padded_size)], 
        radius=radius + 12, fill=255
    )
    padded.putalpha(padded_mask)
    
    # Paste the logo onto the padded background
    padded.paste(logo, (12, 12), logo)
    
    # Draw the letter 'A' on top
    draw = ImageDraw.Draw(padded)
    
    # Try a few common fonts; fall back to PIL default if none work
    font = None
    for font_path in [
        "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
        "/System/Library/Fonts/Supplemental/Arial Bold.ttf",
        "/Library/Fonts/Arial Bold.ttf",
        "C:\\Windows\\Fonts\\arialbd.ttf",
    ]:
        try:
            if os.path.exists(font_path):
                font = ImageFont.truetype(font_path, int(size * 0.55))
                break
        except Exception:
            pass
    
    if font is None:
        font = ImageFont.load_default()
    
    # Center the 'A'
    text = "A"
    bbox = draw.textbbox((0, 0), text, font=font)
    text_w = bbox[2] - bbox[0]
    text_h = bbox[3] - bbox[1]
    x = (padded_size - text_w) // 2 - bbox[0]
    y = (padded_size - text_h) // 2 - bbox[1] - 4  # small visual lift
    draw.text((x, y), text, font=font, fill=(10, 14, 26, 255))
    
    return padded


def generate_aurora_qr(
    url: str,
    output_path: str = "aurora_demo_qr.png",
    box_size: int = 14,
    border: int = 4,
    add_caption: bool = True,
) -> str:
    """
    Generate a stylish branded QR code for the Aurora Live Demo.
    
    Args:
        url: The full demo URL (must be publicly accessible)
        output_path: where to write the PNG
        box_size: pixel size of each QR module
        border: number of quiet-zone modules
        add_caption: whether to add a caption strip below
    
    Returns:
        absolute path of the generated file
    """
    # Use HIGH error correction (30%) so the QR still scans
    # even with the logo covering the center
    qr = qrcode.QRCode(
        version=None,
        error_correction=qrcode.constants.ERROR_CORRECT_H,
        box_size=box_size,
        border=border,
    )
    qr.add_data(url)
    qr.make(fit=True)
    
    # Build the QR with rounded modules and Aurora-toned colors
    img = qr.make_image(
        image_factory=StyledPilImage,
        module_drawer=RoundedModuleDrawer(radius_ratio=1.0),
        color_mask=SolidFillColorMask(
            back_color=(255, 255, 255),
            front_color=(20, 24, 45),  # Aurora deep navy
        ),
    ).convert("RGBA")
    
    # Embed Aurora logo at the center
    logo_size = img.size[0] // 5
    logo = make_logo(size=logo_size)
    pos = ((img.size[0] - logo.size[0]) // 2, (img.size[1] - logo.size[1]) // 2)
    img.paste(logo, pos, logo)
    
    # Optionally add a caption strip below the QR
    if add_caption:
        qr_w, qr_h = img.size
        caption_h = 110
        canvas = Image.new("RGBA", (qr_w, qr_h + caption_h), (255, 255, 255, 255))
        canvas.paste(img, (0, 0))
        
        draw = ImageDraw.Draw(canvas)
        
        font_big = font_small = None
        for font_path in [
            "/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
            "/System/Library/Fonts/Supplemental/Arial Bold.ttf",
        ]:
            try:
                if os.path.exists(font_path):
                    font_big = ImageFont.truetype(font_path, 28)
                    break
            except Exception:
                pass
        for font_path in [
            "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
            "/System/Library/Fonts/Supplemental/Arial.ttf",
        ]:
            try:
                if os.path.exists(font_path):
                    font_small = ImageFont.truetype(font_path, 18)
                    break
            except Exception:
                pass
        
        if font_big is None:
            font_big = ImageFont.load_default()
        if font_small is None:
            font_small = ImageFont.load_default()
        
        # Title
        title = "Project Aurora — Live Demo"
        bbox = draw.textbbox((0, 0), title, font=font_big)
        tw = bbox[2] - bbox[0]
        draw.text(
            ((qr_w - tw) // 2, qr_h + 18),
            title, font=font_big, fill=(20, 24, 45)
        )
        
        # Subtitle
        subtitle = "Scan with LINE or WeChat to experience"
        bbox = draw.textbbox((0, 0), subtitle, font=font_small)
        sw = bbox[2] - bbox[0]
        draw.text(
            ((qr_w - sw) // 2, qr_h + 56),
            subtitle, font=font_small, fill=(100, 110, 130)
        )
        
        # Small URL line for context (if URL is reasonable length)
        if len(url) <= 60:
            url_display = url
        else:
            url_display = url[:30] + "..." + url[-20:]
        bbox = draw.textbbox((0, 0), url_display, font=font_small)
        uw = bbox[2] - bbox[0]
        draw.text(
            ((qr_w - uw) // 2, qr_h + 82),
            url_display, font=font_small, fill=(127, 119, 221)
        )
        
        img = canvas
    
    # Save
    img.convert("RGB").save(output_path, "PNG", dpi=(300, 300))
    
    return os.path.abspath(output_path)


def main():
    # Default demo URL — replace with your actual deployed URL
    DEMO_URL = "https://example.netlify.app/aurora_live_demo.html"
    
    if len(sys.argv) > 1:
        DEMO_URL = sys.argv[1]
    
    output = "aurora_demo_qr.png"
    if len(sys.argv) > 2:
        output = sys.argv[2]
    
    print(f"Generating QR code for:")
    print(f"  URL: {DEMO_URL}")
    print(f"  Output: {output}")
    print()
    
    path = generate_aurora_qr(DEMO_URL, output)
    print(f"✓ Saved: {path}")
    print()
    print("Test the QR code by:")
    print("  1. Opening the PNG on screen")
    print("  2. Scanning with LINE / WeChat / native camera")
    print("  3. Confirming it opens the demo URL in browser")


if __name__ == "__main__":
    main()
