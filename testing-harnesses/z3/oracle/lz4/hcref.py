#!/usr/bin/env python3
"""LZ4HC reference oracle (python-lz4 4.4.5 = liblz4 1.9.4). usage: hcref.py LEVEL < input > raw LZ4 block"""
import sys, lz4.block
sys.stdout.buffer.write(lz4.block.compress(sys.stdin.buffer.read(), mode='high_compression', compression=int(sys.argv[1]), store_size=False))
